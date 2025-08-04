docker buildx build --platform linux/amd64 -f Dockerfile \
 --tag meinya/dedup:$(git describe --tags --dirty --always) \
 --tag meinya/dedup:latest --push .

echo $(git describe --tags --dirty --always)