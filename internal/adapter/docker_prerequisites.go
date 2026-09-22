package adapter

import "regexp"

var prerequisiteUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func remoteUserPatternForPrerequisites(user string) bool {
	return prerequisiteUserPattern.MatchString(user)
}

func dockerPrerequisiteScript(user string) string {
	return `set -eu
. /etc/os-release
case "${ID:-}:${ID_LIKE:-}" in
  *ubuntu*|*debian*) ;;
  *) echo "Ranch Hand supports guided Docker installation only on Ubuntu or Debian" >&2; exit 64 ;;
esac
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install --yes docker.io
if apt-cache show docker-compose-v2 >/dev/null 2>&1; then
  apt-get install --yes docker-compose-v2
elif apt-cache show docker-compose-plugin >/dev/null 2>&1; then
  apt-get install --yes docker-compose-plugin
else
  echo "The configured apt repositories do not provide Docker Compose v2" >&2
  exit 65
fi
if command -v systemctl >/dev/null 2>&1; then systemctl enable --now docker; else service docker start; fi
getent group docker >/dev/null 2>&1 || groupadd docker
usermod -aG docker ` + shellQuote(user) + `
docker version --format '{{.Server.Version}}/{{.Server.Os}}/{{.Server.Arch}}'
docker compose version --short
`
}
