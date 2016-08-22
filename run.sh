#!/bin/sh

# MAINTAINER Calum Gardner <calum@qubit.com>
DIR=$(dirname "$FILE_TO_WRITE")
mkdir -p $DIR

touch $FILE_TO_WRITE

./gcp-discoverer
