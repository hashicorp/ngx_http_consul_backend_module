IMAGE_TAG ?= hashicorp/nginx-consul

docker:
	@docker build -t "${IMAGE_TAG}" -f docker/build.Dockerfile .
	@docker run -it -p 8080:80 --rm "${IMAGE_TAG}"
.PHONY: docker

deps:
	@cd src/ && dep ensure -update
.PHONY: deps
