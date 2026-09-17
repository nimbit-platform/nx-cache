.PHONY: generate css test build docker tidy

generate:
	templ generate

css:
	npx @tailwindcss/cli -i assets/css/globals.css -o internal/web/static/app.css --minify

tidy:
	go mod tidy

test: generate
	go test ./...

build: generate css
	go build -o bin/nx-cache ./cmd/server

docker:
	docker build -t nx-cache:local .
