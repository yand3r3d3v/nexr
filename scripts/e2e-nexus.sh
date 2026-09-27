#!/usr/bin/env bash
# Starts a Nexus Repository container for the end-to-end tests and prints the
# environment that points nexr at it:
#
#   eval "$(scripts/e2e-nexus.sh start 3.96.3)"
#   go test -tags e2e ./test/e2e/...
#   scripts/e2e-nexus.sh stop 3.96.3
#
# The container listens on 127.0.0.1:$NEXR_E2E_PORT (default 18081), and the
# Docker repository docker-e2e has a connector on 127.0.0.1:$NEXR_E2E_DOCKER_PORT
# (default 18082) for docker and crane, which need the registry at the root of
# a host. The script sets the admin password to $NEXR_E2E_PASSWORD (default
# admin123), accepts the Community Edition EULA where the server asks for it,
# disables anonymous access, and creates the hosted repositories raw-e2e and
# docker-e2e. Running it again reuses the container.
set -euo pipefail

usage() {
	echo "usage: $0 start|stop [NEXUS_VERSION]" >&2
	exit 2
}

[ $# -ge 1 ] || usage
action=$1
version=${2:-3.96.3}
port=${NEXR_E2E_PORT:-18081}
docker_port=${NEXR_E2E_DOCKER_PORT:-18082}
password=${NEXR_E2E_PASSWORD:-admin123}
name=nexr-e2e-${version}
url=http://127.0.0.1:${port}
api=${url}/service/rest

log() { echo "e2e-nexus: $*" >&2; }

# api_admin METHOD PATH [curl args...] sends an authenticated API request.
api_admin() {
	local method=$1 path=$2
	shift 2
	curl -fsS -u "admin:${password}" -X "$method" "$@" "${api}${path}"
}

start() {
	if [ -z "$(docker ps -q --filter "name=^${name}$")" ]; then
		docker rm -f "$name" >/dev/null 2>&1 || true
		log "starting sonatype/nexus3:${version} on ${url}"
		docker run -d --name "$name" -p "127.0.0.1:${port}:8081" -p "127.0.0.1:${docker_port}:5000" \
			"sonatype/nexus3:${version}" >/dev/null
	fi

	log "waiting for Nexus to start (this takes a minute or two)"
	local i
	for i in $(seq 1 120); do
		if curl -fs -o /dev/null "${api}/v1/status"; then
			break
		fi
		if [ "$i" -eq 120 ]; then
			log "Nexus did not start in time"
			docker logs --tail 50 "$name" >&2
			exit 1
		fi
		sleep 5
	done

	# A fresh server has a generated admin password.
	if ! curl -fs -o /dev/null -u "admin:${password}" "${api}/v1/repositories"; then
		log "setting the admin password"
		local generated
		generated=$(docker exec "$name" cat /nexus-data/admin.password)
		curl -fsS -u "admin:${generated}" -X PUT -H 'Content-Type: text/plain' \
			--data "$password" "${api}/v1/security/users/admin/change-password"
	fi

	# Community Edition 3.77 and newer ask for the EULA; older releases have no such endpoint.
	local eula
	if eula=$(curl -fs -u "admin:${password}" "${api}/v1/system/eula"); then
		if printf '%s' "$eula" | grep -Eq '"accepted"[[:space:]]*:[[:space:]]*false'; then
			log "accepting the Community Edition EULA"
			printf '%s' "$eula" | sed -E 's/"accepted"[[:space:]]*:[[:space:]]*false/"accepted": true/' |
				api_admin POST /v1/system/eula -H 'Content-Type: application/json' --data-binary @-
		fi
	fi

	# A fresh 3.71 enables anonymous access, a fresh 3.96 does not: set it explicitly.
	api_admin PUT /v1/security/anonymous -o /dev/null -H 'Content-Type: application/json' \
		--data '{"enabled": false, "userId": "anonymous", "realmName": "NexusAuthorizingRealm"}'

	create_repo raw raw-e2e \
		'{"name": "raw-e2e", "online": true, "storage": {"blobStoreName": "default", "strictContentTypeValidation": false, "writePolicy": "ALLOW"}}'
	create_repo docker docker-e2e \
		'{"name": "docker-e2e", "online": true, "storage": {"blobStoreName": "default", "strictContentTypeValidation": true, "writePolicy": "ALLOW"}, "docker": {"v1Enabled": false, "forceBasicAuth": true, "httpPort": 5000}}'

	log "ready"
	printf 'export NEXUS_URL=%s NEXUS_USER=admin NEXUS_PASSWORD=%s NEXR_E2E_REGISTRY=127.0.0.1:%s\n' "$url" "$password" "$docker_port"
}

# create_repo FORMAT NAME JSON creates a hosted repository unless it exists.
create_repo() {
	if curl -fs -o /dev/null -u "admin:${password}" "${api}/v1/repositories/$2"; then
		return
	fi
	log "creating repository $2"
	api_admin POST "/v1/repositories/$1/hosted" -H 'Content-Type: application/json' --data "$3"
}

stop() {
	docker rm -f "$name" >/dev/null
	log "removed ${name}"
}

case $action in
start) start ;;
stop) stop ;;
*) usage ;;
esac
