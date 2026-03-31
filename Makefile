.PHONY: build build-linux deploy clean test

VERSION ?= dev

build:
	go build -ldflags="-s -w -X main.Version=$(VERSION)" -o vd .

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.Version=$(VERSION)" -o vd-linux-amd64 .

deploy:
	@test -n "$(SERVER)" || (echo "Usage: make deploy SERVER=root@server [DOMAIN=apps.example.com] [PROD_DB=container] [PROD_DB_USER=user]" && exit 1)
	./scripts/deploy.sh $(SERVER) $(DOMAIN) $(PROD_DB) $(PROD_DB_USER)

clean:
	rm -f vd vd-linux-*

test:
	go test ./...
