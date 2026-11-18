#!/bin/sh
set -eu

releases=${FACTORY_RELEASES:-https://github.com/afonsodelgado/factory/releases/latest/download}
bin=$HOME/.local/bin

fail() {
	echo "factory install: $*" >&2
	exit 1
}

case $(uname -s) in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "factory runs on macOS and Linux" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "no factory build for $(uname -m)" ;;
esac
command -v curl >/dev/null 2>&1 || fail "install curl, then run this again"

asset=factory_${os}_${arch}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "$releases/$asset" -o "$tmp/$asset" || fail "could not download $asset"
curl -fsSL "$releases/factory_checksums.txt" -o "$tmp/checksums" || fail "could not download the checksums"
want=$(awk -v asset="$asset" '$2 == asset || $2 == "*" asset { print $1 }' "$tmp/checksums")
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$asset" | cut -d ' ' -f 1)
else
	got=$(shasum -a 256 "$tmp/$asset" | cut -d ' ' -f 1)
fi
[ -n "$want" ] && [ "$want" = "$got" ] || fail "$asset does not match its checksum, so nothing was installed"

mkdir -p "$bin"
chmod 755 "$tmp/$asset"
mv "$tmp/$asset" "$bin/factory"
echo "Installed $("$bin/factory" --version) at $bin/factory"

case ":$PATH:" in
*":$bin:"*) ;;
*)
	case ${SHELL##*/} in
	zsh) profile=$HOME/.zshrc ;;
	bash) profile=$HOME/.bashrc ;;
	*) profile=$HOME/.profile ;;
	esac
	line="export PATH=\"\$HOME/.local/bin:\$PATH\""
	grep -qsF "$line" "$profile" || printf '\n%s\n' "$line" >>"$profile"
	echo "Added ~/.local/bin to PATH in $profile; open a new shell to pick it up"
	;;
esac

if [ -t 1 ] && { : </dev/tty; } 2>/dev/null; then
	exec "$bin/factory" setup "$@" </dev/tty
fi
exec "$bin/factory" setup "$@"
