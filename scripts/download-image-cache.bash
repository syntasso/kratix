#!/usr/bin/env bash

function save_image() {
  docker pull $1
  docker save $1 -o $2
}

mkdir -p $PWD/image-cache/

while read p; do
	location=$PWD/image-cache/$(echo "$p" | tr / _).tar
	if [[ ! -f "$location" ]]; then
		echo "saving $p to $location"
		save_image $p $location &
	fi
done <images

wait < <(jobs -p)
