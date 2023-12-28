#!/usr/bin/env bash

ROOT=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/.." &> /dev/null && pwd )

set -eu

cd $ROOT

git checkout main
git merge --no-ff --no-edit dev

$ROOT/scripts/make-distribution