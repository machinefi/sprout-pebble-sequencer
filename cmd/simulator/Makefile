#NAME=$(shell basename $$PWD)
NAME=simulator
FEATURE=$(shell git rev-parse --abbrev-ref HEAD)
VERSION=$(shell git describe --tags --always)
COMMITID=$(shell git rev-parse --short --show HEAD)
DATE=$(shell TZ=Asia/Shanghai date +%Y%m%d%H%M%S)

VERSION_PATH=main

LDFLAGS="-s -w \
	-X ${VERSION_PATH}.Name=${NAME}         \
	-X ${VERSION_PATH}.Feature=${FEATURE}   \
	-X ${VERSION_PATH}.Version=${VERSION}   \
	-X ${VERSION_PATH}.CommitID=${COMMITID} \
	-X ${VERSION_PATH}.Date=${DATE} "


STATIC_LDFLAGS="-linkmode 'external' -extldflags '-static' -s -w   \
	-X ${VERSION_PATH}.Name=${NAME}         \
	-X ${VERSION_PATH}.Feature=${FEATURE}   \
	-X ${VERSION_PATH}.Version=${VERSION}   \
	-X ${VERSION_PATH}.CommitID=${COMMITID} \
	-X ${VERSION_PATH}.Date=${DATE} "


build: clean
	CGO_ENABLE=0 go build -ldflags ${LDFLAGS} -o ${NAME}

build_static: clean
	CGO_ENABLE=0 go build -ldflags ${STATIC_LDFLAGS} -o ${NAME}

run: build
	./${NAME}

clean:
	@rm -rf ${NAME}

