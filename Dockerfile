# Étape 1 : Compilation des hooks
FROM golang:1.25.6-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Compilation du binaire (nommé 'pocketbase')
RUN GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o pocketbase

# Étape 2 : Image finale légère
FROM alpine:latest
RUN apk add --no-cache \
    unzip \
    ca-certificates
WORKDIR /pb

# Copie du binaire compilé
COPY --from=builder /app/pocketbase /pb/pocketbase
# Copie éventuelle des fichiers statiques ou migrations
COPY --from=builder /app/pb_migrations /pb/pb_migrations

# Création du volume pour les données (important pour la persistance)
VOLUME /pb/pb_data

EXPOSE 8090

# Commande de lancement
CMD ["/pb/pocketbase", "serve"]