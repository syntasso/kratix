#!/usr/bin/env bash


while true
do
	echo "starting manager"
	echo ""
	echo ""
	./controllers
	exit_code=$?
	echo ""
	echo ""
	echo "got exist code $exit_code"
	if [ $exit_code != 124 ];then
		echo "quiting"
		exit 1
	fi
done
