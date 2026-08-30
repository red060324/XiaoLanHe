FROM node:22-alpine AS web
WORKDIR /src/frontend/xiaolanhe-web
COPY frontend/xiaolanhe-web/package*.json ./
RUN npm ci
COPY frontend/xiaolanhe-web/ ./
RUN npm run build

FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /xiaolanhe ./cmd/xiaolanhe

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /xiaolanhe ./xiaolanhe
COPY --from=build /src/prompts ./prompts
COPY --from=web /src/frontend/xiaolanhe-web/dist ./frontend/xiaolanhe-web/dist
EXPOSE 10000
ENTRYPOINT ["./xiaolanhe"]
