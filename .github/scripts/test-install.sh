#!/usr/bin/env bash
# Installs RESH from a release archive and checks that it works
# Usage: test-install.sh <path to resh_*_linux_*.tar.gz>
set -euo pipefail

archive=$(realpath "$1")
workdir=$(mktemp -d)
tar -xzf "$archive" -C "$workdir"

# Skip the interactive device name prompt
mkdir -p ~/.local/share/resh
echo ci > ~/.local/share/resh/device-name
# install.sh only sets up zsh if ~/.zshrc exists
if command -v zsh >/dev/null; then
    touch ~/.zshrc
fi

echo "Installing ..."
(cd "$workdir" && SHELL=/bin/bash scripts/install.sh)

echo "Checking installed files ..."
for f in ~/.resh/shellrc ~/.resh/hooks.sh ~/.resh/bin/reshctl ~/.resh/bin/resh-daemon ~/.bash-preexec.sh; do
    [ -e "$f" ] || { echo "Missing: $f"; exit 1; }
done
grep -q 'source ~/.resh/shellrc' ~/.bashrc || { echo "$HOME/.bashrc doesn't load RESH"; exit 1; }

# Loading RESH in an interactive shell starts the daemon
check_shell() {
    echo "Checking RESH in $1 ..."
    # shellcheck disable=SC2016 # expanded by the interactive shell
    "$1" -ic '
        [ -n "${__RESH_VERSION-}" ] || { echo "RESH is not loaded"; exit 1; }
        for _ in $(seq 20); do
            reshctl version > /tmp/resh-version 2>&1
            grep -q "Currently running daemon" /tmp/resh-version && break
            sleep 0.5
        done
        cat /tmp/resh-version
        grep -q "Currently running daemon" /tmp/resh-version
    ' || { echo "RESH doesn't work in $1"; exit 1; }
}

check_shell bash
if command -v zsh >/dev/null; then
    check_shell zsh
fi

echo "OK"
