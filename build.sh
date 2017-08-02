docker run --rm -v "$PWD/":/project -u $(id -u) desertbit/golang-gb:alpine /bin/sh -c "gb build"
