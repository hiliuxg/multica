#!/usr/bin/env bash
set -Eeuo pipefail

# Build a complete offline Multica server package for linux/amd64 from the
# current local checkout. The target server only needs to docker load the image
# archives and run Docker Compose; it never compiles source code.

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"

version="${VERSION:-}"
proxy_url="${PROXY_URL:-http://127.0.0.1:7897}"
output_root="${OUTPUT_DIR:-$script_dir/dist}"
cache_root="${CACHE_DIR:-$script_dir/.cache}"
nginx_image="nginx:1.29.8-alpine"
force=0
refresh_images=0
keep_workdir="${KEEP_WORKDIR:-0}"

usage() {
  cat <<'EOF'
Usage: deploy/build-linux-amd64.sh [options]

Build backend and Web images from the current checkout for linux/amd64, pull
the nginx gateway and pgvector runtime images, and create an offline Docker
Compose server package.

Options:
  --version VERSION    Release version, for example v0.4.35 (default: exact Git tag)
  --proxy URL          Host proxy URL (default: http://127.0.0.1:7897)
  --output-dir DIR     Output directory (default: deploy/dist)
  --cache-dir DIR      Persistent image/build cache (default: deploy/.cache)
  --refresh-images     Re-download base images, nginx, and pgvector into the cache
  --force              Replace the same version's existing output
  -h, --help           Show this help

Environment overrides:
  VERSION, PROXY_URL, OUTPUT_DIR, CACHE_DIR
  DOCKER_BUILDER_HOST  Docker socket used for image builds
  DOCKER_PROXY_URL     Proxy URL reachable from Docker build containers
  BUILD_MEMORY_GB      Temporary Colima memory in GiB (default: 12)
  KEEP_WORKDIR=1       Keep the temporary build directory for diagnosis

On Apple Silicon with less than 8 GiB assigned to Docker, the script creates an
isolated temporary 12 GiB Colima profile and deletes it after packaging. It does
not restart or modify the user's existing Docker/Colima environment.
EOF
}

die() {
  echo "ERROR: $*" >&2
  exit 1
}

log() {
  echo
  echo "==> $*"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"
}

retry() {
  local max_attempts="$1"
  shift
  local attempt=1

  until "$@"; do
    if ((attempt >= max_attempts)); then
      return 1
    fi
    attempt=$((attempt + 1))
    echo "Retrying ($attempt/$max_attempts): $*" >&2
    sleep 5
  done
}

cached_image_is_valid() {
  local archive="$1"
  local expected_ref="$2"
  local expected_platform="$3"
  local config_file
  local actual_platform

  [[ -f "$archive" ]] || return 1
  crane validate --tarball "$archive" >/dev/null 2>&1 || return 1
  tar -xOf "$archive" manifest.json \
    | jq -e --arg ref "$expected_ref" '.[0].RepoTags | index($ref) != null' \
      >/dev/null 2>&1 || return 1
  config_file="$(tar -xOf "$archive" manifest.json | jq -r '.[0].Config')" || return 1
  actual_platform="$(
    tar -xOf "$archive" "$config_file" | jq -r '.os + "/" + .architecture'
  )" || return 1
  [[ "$actual_platform" == "$expected_platform" ]]
}

ensure_cached_image() {
  local image_ref="$1"
  local image_platform="$2"
  local cache_file="$3"
  local download_file="$work_dir/$(basename "$cache_file").download"

  if ((refresh_images == 0)) &&
    cached_image_is_valid "$cache_file" "$image_ref" "$image_platform"; then
    echo "CACHE HIT: $image_ref ($image_platform)"
    return
  fi

  if ((refresh_images == 1)); then
    echo "REFRESH:   $image_ref ($image_platform)"
  elif [[ -e "$cache_file" ]]; then
    echo "REPAIR:    $image_ref ($image_platform); cached archive is invalid"
  else
    echo "CACHE MISS: $image_ref ($image_platform)"
  fi

  retry 3 crane pull --platform "$image_platform" \
    "$image_ref" "$download_file"
  cached_image_is_valid "$download_file" "$image_ref" "$image_platform" ||
    die "Downloaded image archive failed validation: $image_ref ($image_platform)"
  mv "$download_file" "$cache_file"
  echo "CACHED:    $cache_file"
}

while (($# > 0)); do
  case "$1" in
    --version)
      (($# >= 2)) || die "--version requires a value"
      version="$2"
      shift 2
      ;;
    --proxy)
      (($# >= 2)) || die "--proxy requires a value"
      proxy_url="$2"
      shift 2
      ;;
    --output-dir)
      (($# >= 2)) || die "--output-dir requires a value"
      output_root="$2"
      shift 2
      ;;
    --cache-dir)
      (($# >= 2)) || die "--cache-dir requires a value"
      cache_root="$2"
      shift 2
      ;;
    --refresh-images)
      refresh_images=1
      shift
      ;;
    --force)
      force=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "Unknown option: $1"
      ;;
  esac
done

for command_name in docker git crane jq shasum tar openssl sed awk; do
  require_command "$command_name"
done

cd "$repo_root"

if [[ -z "$version" ]]; then
  version="$(git describe --tags --match 'v[0-9]*' --exact-match HEAD 2>/dev/null || true)"
fi
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] ||
  die "Cannot determine a release version. Pass --version vX.Y.Z."

commit="$(git rev-parse HEAD)"
build_date="$(git show -s --format=%cI "$commit")"
image_tag="${version}-local"
package_name="multica-${version}-linux-amd64-server"

if [[ "$output_root" != /* ]]; then
  output_root="$repo_root/$output_root"
fi
if [[ "$cache_root" != /* ]]; then
  cache_root="$repo_root/$cache_root"
fi
mkdir -p "$output_root"
image_cache_dir="$cache_root/images"
build_cache_root="$cache_root/buildx"
mkdir -p "$image_cache_dir" "$build_cache_root"

package_dir="$output_root/$package_name"
server_archive="$output_root/$package_name.tar.gz"
release_checksums="$output_root/multica-${version}-SHA256SUMS"

if [[ -e "$package_dir" || -e "$server_archive" || -e "$release_checksums" ]]; then
  if ((force == 0)); then
    die "Output already exists for $version. Re-run with --force to replace it."
  fi
  case "$package_dir" in
    "$output_root"/multica-v*-linux-amd64-server) ;;
    *) die "Refusing to replace unexpected package path: $package_dir" ;;
  esac
  rm -rf -- "$package_dir"
  rm -f -- "$server_archive" "$release_checksums"
fi

task_tmp_root="${TMPDIR:-/tmp}"
work_dir="$(mktemp -d "$task_tmp_root/multica-linux-amd64.XXXXXX")"
stage_dir="$work_dir/$package_name"
mkdir -p "$stage_dir/images"

temporary_colima_profile=""

cleanup() {
  local exit_status=$?

  if [[ -n "$temporary_colima_profile" ]]; then
    echo "==> Removing temporary Colima profile: $temporary_colima_profile"
    colima delete "$temporary_colima_profile" --data --force >/dev/null 2>&1 || true
  fi

  if [[ "$keep_workdir" == "1" ]]; then
    echo "Temporary build directory kept at: $work_dir"
  else
    case "$work_dir" in
      "$task_tmp_root"/multica-linux-amd64.*) rm -rf -- "$work_dir" ;;
      *) echo "Refusing to remove unexpected work directory: $work_dir" >&2 ;;
    esac
  fi

  exit "$exit_status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

export HTTP_PROXY="$proxy_url"
export HTTPS_PROXY="$proxy_url"
export http_proxy="$proxy_url"
export https_proxy="$proxy_url"

docker_builder_host="${DOCKER_BUILDER_HOST:-}"
docker_proxy_url="${DOCKER_PROXY_URL:-}"
docker_command=(docker)
if [[ -n "$docker_builder_host" ]]; then
  docker_command=(env "DOCKER_HOST=$docker_builder_host" docker)
fi

"${docker_command[@]}" info >/dev/null 2>&1 || die "Docker is not reachable"
docker_memory_bytes="$("${docker_command[@]}" info --format '{{.MemTotal}}')"
minimum_docker_memory=$((8 * 1024 * 1024 * 1024))

if ((docker_memory_bytes < minimum_docker_memory)); then
  if [[ "$(uname -s)" == "Darwin" && "$(uname -m)" == "arm64" ]] && command -v colima >/dev/null 2>&1; then
    temporary_colima_profile="multica-package-$$"
    build_memory_gb="${BUILD_MEMORY_GB:-12}"
    log "Docker memory is below 8 GiB; starting isolated Colima profile $temporary_colima_profile (${build_memory_gb} GiB)"
    colima start "$temporary_colima_profile" \
      --cpus 4 \
      --memory "$build_memory_gb" \
      --disk 50 \
      --vm-type vz \
      --vz-rosetta \
      --runtime docker \
      --activate=false

    docker_builder_host="unix://$HOME/.colima/$temporary_colima_profile/docker.sock"
    docker_command=(env "DOCKER_HOST=$docker_builder_host" docker)
    "${docker_command[@]}" info >/dev/null 2>&1 || die "Temporary Colima Docker is not reachable"

    if [[ -z "$docker_proxy_url" ]]; then
      colima_gateway="$(
        colima --profile "$temporary_colima_profile" ssh -- \
          sh -lc "ip route show default | awk '{print \$3; exit}'" 2>/dev/null
      )"
      [[ -n "$colima_gateway" ]] || die "Cannot determine the temporary Colima gateway"
      if [[ "$proxy_url" =~ ^(https?://)(127\.0\.0\.1|localhost)(:[0-9]+.*)$ ]]; then
        docker_proxy_url="${BASH_REMATCH[1]}${colima_gateway}${BASH_REMATCH[3]}"
      else
        docker_proxy_url="$proxy_url"
      fi
    fi
  else
    die "Docker has less than 8 GiB memory. Assign at least 8 GiB or set DOCKER_BUILDER_HOST."
  fi
fi

if [[ -z "$docker_proxy_url" ]]; then
  if [[ "$(uname -s)" == "Darwin" ]]; then
    docker_context="$(docker context show 2>/dev/null || true)"
    if [[ "$docker_context" == colima* && "$proxy_url" =~ ^(https?://)(127\.0\.0\.1|localhost)(:[0-9]+.*)$ ]]; then
      docker_proxy_url="${BASH_REMATCH[1]}host.lima.internal${BASH_REMATCH[3]}"
    elif [[ "$proxy_url" =~ ^(https?://)(127\.0\.0\.1|localhost)(:[0-9]+.*)$ ]]; then
      docker_proxy_url="${BASH_REMATCH[1]}host.docker.internal${BASH_REMATCH[3]}"
    else
      docker_proxy_url="$proxy_url"
    fi
  else
    docker_proxy_url="$proxy_url"
  fi
fi

docker_builder_arch="$("${docker_command[@]}" info --format '{{.Architecture}}')"
case "$docker_builder_arch" in
  amd64|x86_64)
    docker_build_platform="linux/amd64"
    ;;
  arm64|aarch64)
    docker_build_platform="linux/arm64"
    ;;
  *)
    die "Unsupported Docker builder architecture: $docker_builder_arch"
    ;;
esac

log "Build provenance"
echo "Version:       $version"
echo "Commit:        $commit"
echo "Target:        linux/amd64"
echo "Builder:       $docker_build_platform"
echo "Output:        $output_root"
echo "Cache:         $cache_root"
echo "Host proxy:    $proxy_url"
echo "Docker proxy:  $docker_proxy_url"

golang_cache="$image_cache_dir/golang-1.26-alpine-${docker_build_platform//\//-}.tar"
alpine_cache="$image_cache_dir/alpine-3.21-linux-amd64.tar"
node_cache="$image_cache_dir/node-22-alpine-linux-amd64.tar"
pgvector_cache="$image_cache_dir/pgvector-pg17-linux-amd64.tar"
nginx_cache="$image_cache_dir/nginx-1.29.8-alpine-linux-amd64.tar"

log "Preparing cached build/runtime base images, nginx, and pgvector"
ensure_cached_image golang:1.26-alpine "$docker_build_platform" "$golang_cache"
ensure_cached_image alpine:3.21 linux/amd64 "$alpine_cache"
ensure_cached_image node:22-alpine linux/amd64 "$node_cache"
ensure_cached_image "$nginx_image" linux/amd64 "$nginx_cache"
ensure_cached_image pgvector/pgvector:pg17 linux/amd64 "$pgvector_cache"

"${docker_command[@]}" load --input "$golang_cache"
"${docker_command[@]}" load --input "$alpine_cache"
"${docker_command[@]}" load --input "$node_cache"
"${docker_command[@]}" load --input "$nginx_cache"
cp "$nginx_cache" "$stage_dir/images/nginx-1.29.8-alpine-linux-amd64.tar"
cp "$pgvector_cache" "$stage_dir/images/pgvector-pg17-linux-amd64.tar"

backend_build_cache="$build_cache_root/backend-${docker_build_platform//\//-}-to-linux-amd64"
web_build_cache="$build_cache_root/web-linux-amd64"
mkdir -p "$backend_build_cache" "$web_build_cache"

backend_cache_options=(
  --cache-to "type=local,dest=$backend_build_cache,mode=max"
)
if [[ -f "$backend_build_cache/index.json" ]]; then
  backend_cache_options=(
    --cache-from "type=local,src=$backend_build_cache"
    "${backend_cache_options[@]}"
  )
  echo "BUILD CACHE: backend cache available"
else
  echo "BUILD CACHE: backend cold build"
fi

web_cache_options=(
  --cache-to "type=local,dest=$web_build_cache,mode=max"
)
if [[ -f "$web_build_cache/index.json" ]]; then
  web_cache_options=(
    --cache-from "type=local,src=$web_build_cache"
    "${web_cache_options[@]}"
  )
  echo "BUILD CACHE: Web cache available"
else
  echo "BUILD CACHE: Web cold build"
fi

build_backend_image() {
  rm -f -- "$work_dir/backend-image.tar"
  "${docker_command[@]}" buildx build \
    --progress plain \
    --network host \
    --platform linux/amd64 \
    --build-arg "HTTP_PROXY=$docker_proxy_url" \
    --build-arg "HTTPS_PROXY=$docker_proxy_url" \
    --build-arg "http_proxy=$docker_proxy_url" \
    --build-arg "https_proxy=$docker_proxy_url" \
    --build-arg "VERSION=$version" \
    --build-arg "COMMIT=$commit" \
    --build-arg "DATE=$build_date" \
    "${backend_cache_options[@]}" \
    --tag "multica-backend:$image_tag" \
    --output "type=docker,dest=$work_dir/backend-image.tar" \
    --file "$script_dir/Dockerfile.linux.amd64" \
    "$repo_root"
}

log "Building backend image from the current checkout"
retry 2 build_backend_image
mv "$work_dir/backend-image.tar" \
  "$stage_dir/images/multica-backend-${version}-linux-amd64.tar"

build_web_image() {
  rm -f -- "$work_dir/web-image.tar"
  "${docker_command[@]}" buildx build \
    --progress plain \
    --network host \
    --platform linux/amd64 \
    --build-arg "HTTP_PROXY=$docker_proxy_url" \
    --build-arg "HTTPS_PROXY=$docker_proxy_url" \
    --build-arg "http_proxy=$docker_proxy_url" \
    --build-arg "https_proxy=$docker_proxy_url" \
    --build-arg "NEXT_PUBLIC_APP_VERSION=$version" \
    "${web_cache_options[@]}" \
    --tag "multica-web:$image_tag" \
    --output "type=docker,dest=$work_dir/web-image.tar" \
    --file "$repo_root/Dockerfile.web" \
    "$repo_root"
}

log "Building Web image from the current checkout"
retry 2 build_web_image
mv "$work_dir/web-image.tar" \
  "$stage_dir/images/multica-web-${version}-linux-amd64.tar"

log "Writing offline deployment files"
cp "$repo_root/docker-compose.selfhost.yml" "$stage_dir/docker-compose.yml"
cp "$script_dir/README.md" "$stage_dir/README.md"
cp "$script_dir/nginx.conf" "$stage_dir/nginx.conf"

cat > "$stage_dir/VERSION" <<EOF
version=$version
commit=$commit
platform=linux/amd64
source=local-checkout
image_tag=$image_tag
EOF

cat > "$stage_dir/.env.example" <<EOF
POSTGRES_DB=multica
POSTGRES_USER=multica
POSTGRES_PASSWORD=CHANGE_ME_POSTGRES_PASSWORD

PORT=8080
FRONTEND_PORT=3000
GATEWAY_BIND_ADDRESS=127.0.0.1
GATEWAY_PORT=18080
APP_ENV=production

JWT_SECRET=CHANGE_ME_JWT_SECRET
MULTICA_VCS_SECRET_KEY=CHANGE_ME_VCS_SECRET_KEY

FRONTEND_ORIGIN=https://multica.example.com
MULTICA_APP_URL=https://multica.example.com
MULTICA_PUBLIC_URL=https://multica.example.com
MULTICA_DAEMON_SERVER_URL=https://multica.example.com
GOOGLE_REDIRECT_URI=https://multica.example.com/auth/callback
MULTICA_TRUSTED_PROXIES=

AUTH_MODE=tmeoa
TPP_APPSECRET=CHANGE_ME_TPP_APPSECRET
TMEOA_MAX_CLOCK_SKEW=5m

MULTICA_IMAGE_TAG=$image_tag
MULTICA_BACKEND_IMAGE=multica-backend
MULTICA_WEB_IMAGE=multica-web
MULTICA_NGINX_IMAGE=$nginx_image
MULTICA_NGINX_CONFIG=./nginx.conf

ALLOW_SIGNUP=false
ALLOWED_EMAIL_DOMAINS=tencentmusic.com
RESEND_API_KEY=
RESEND_FROM_EMAIL=noreply@multica.example.com
SMTP_HOST=
SMTP_PORT=25
SMTP_USERNAME=
SMTP_PASSWORD=
SMTP_FROM_EMAIL=
SMTP_TLS=

MULTICA_LLM_API_KEY=
MULTICA_LLM_BASE_URL=
MULTICA_LLM_DEFAULT_MODEL=
EOF

cat > "$stage_dir/prepare-env.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

domain="${1:-}"
gateway_bind_address="${2:-127.0.0.1}"
if [[ -z "$domain" || "$domain" == *"://"* || "$domain" == */* ]]; then
  echo "Usage: $0 multica.example.com [gateway-bind-ip]" >&2
  exit 1
fi
valid_gateway_bind_address=1
if [[ "$gateway_bind_address" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
  IFS=. read -r octet1 octet2 octet3 octet4 <<<"$gateway_bind_address"
  for octet in "$octet1" "$octet2" "$octet3" "$octet4"; do
    if ((10#$octet > 255)); then
      valid_gateway_bind_address=0
    fi
  done
else
  valid_gateway_bind_address=0
fi
if ((valid_gateway_bind_address == 0)) || [[ "$gateway_bind_address" == "0.0.0.0" ]]; then
  echo "gateway-bind-ip must be an exact IPv4 address, not 0.0.0.0" >&2
  exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
env_file="$script_dir/.env"
template="$script_dir/.env.example"

test -f "$template" || {
  echo "Missing template: $template" >&2
  exit 1
}
test ! -e "$env_file" || {
  echo "Refusing to overwrite existing file: $env_file" >&2
  exit 1
}

postgres_password="$(openssl rand -hex 24)"
jwt_secret="$(openssl rand -hex 32)"
vcs_secret="$(openssl rand -base64 32 | tr -d '\n')"

sed \
  -e "s|CHANGE_ME_POSTGRES_PASSWORD|$postgres_password|" \
  -e "s|CHANGE_ME_JWT_SECRET|$jwt_secret|" \
  -e "s|CHANGE_ME_VCS_SECRET_KEY|$vcs_secret|" \
  -e "s|^GATEWAY_BIND_ADDRESS=.*|GATEWAY_BIND_ADDRESS=$gateway_bind_address|" \
  -e "s|multica\.example\.com|$domain|g" \
  "$template" > "$env_file"

chmod 0600 "$env_file"
echo "Created $env_file"
EOF
chmod 0755 "$stage_dir/prepare-env.sh"

cat > "$stage_dir/load-images.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
version="$(sed -n 's/^version=//p' "$script_dir/VERSION")"
image_tag="$(sed -n 's/^image_tag=//p' "$script_dir/VERSION")"

for archive in \
  "$script_dir/images/nginx-1.29.8-alpine-linux-amd64.tar" \
  "$script_dir/images/pgvector-pg17-linux-amd64.tar" \
  "$script_dir/images/multica-backend-${version}-linux-amd64.tar" \
  "$script_dir/images/multica-web-${version}-linux-amd64.tar"; do
  test -f "$archive" || {
    echo "Missing image archive: $archive" >&2
    exit 1
  }
  docker load --input "$archive"
done

for image in \
  "nginx:1.29.8-alpine" \
  "pgvector/pgvector:pg17" \
  "multica-backend:$image_tag" \
  "multica-web:$image_tag"; do
  platform="$(docker image inspect "$image" --format '{{.Os}}/{{.Architecture}}')"
  test "$platform" = "linux/amd64" || {
    echo "Unexpected platform for $image: $platform" >&2
    exit 1
  }
  echo "$image $platform"
done
EOF
chmod 0755 "$stage_dir/load-images.sh"

log "Validating image archives and deployment configuration"
for image_archive in "$stage_dir"/images/*.tar; do
  crane validate --tarball "$image_archive"
  config_file="$(tar -xOf "$image_archive" manifest.json | jq -r '.[0].Config')"
  archive_platform="$(tar -xOf "$image_archive" "$config_file" | jq -r '.os + "/" + .architecture')"
  [[ "$archive_platform" == "linux/amd64" ]] ||
    die "Unexpected image platform in $image_archive: $archive_platform"
done

bash -n "$stage_dir/load-images.sh"
bash -n "$stage_dir/prepare-env.sh"
"${docker_command[@]}" run --rm --platform linux/amd64 \
  --add-host backend:127.0.0.1 \
  --add-host frontend:127.0.0.1 \
  --volume "$script_dir/nginx.conf:/etc/nginx/conf.d/default.conf:ro" \
  "$nginx_image" nginx -t
docker compose --env-file "$stage_dir/.env.example" \
  -f "$stage_dir/docker-compose.yml" config --quiet

compose_images="$(
  docker compose --env-file "$stage_dir/.env.example" \
    -f "$stage_dir/docker-compose.yml" config --images
)"
grep -qx "multica-backend:$image_tag" <<<"$compose_images" ||
  die "Compose does not reference the locally built backend image"
grep -qx "multica-web:$image_tag" <<<"$compose_images" ||
  die "Compose does not reference the locally built Web image"
grep -qx "$nginx_image" <<<"$compose_images" ||
  die "Compose does not reference $nginx_image"
grep -qx 'pgvector/pgvector:pg17' <<<"$compose_images" ||
  die "Compose does not reference pgvector/pgvector:pg17"

(
  cd "$stage_dir"
  shasum -a 256 \
    .env.example \
    README.md \
    VERSION \
    docker-compose.yml \
    images/*.tar \
    load-images.sh \
    nginx.conf \
    prepare-env.sh \
    | sed 's#  #  ./#' > SHA256SUMS
  shasum -a 256 --check SHA256SUMS
)

log "Creating final server archive"
archive_in_workdir="$work_dir/$package_name.tar.gz"
COPYFILE_DISABLE=1 tar -czf "$archive_in_workdir" -C "$work_dir" "$package_name"

mv "$stage_dir" "$package_dir"
mv "$archive_in_workdir" "$server_archive"
(
  cd "$output_root"
  shasum -a 256 "$(basename "$server_archive")" > "$(basename "$release_checksums")"
  shasum -a 256 --check "$(basename "$release_checksums")"
)

log "Build complete"
echo "Package directory: $package_dir"
echo "Server archive:    $server_archive"
echo "Checksums:         $release_checksums"
echo
cat "$release_checksums"
