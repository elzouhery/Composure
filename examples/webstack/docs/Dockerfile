# escape=\
# Two stages, a cache-breaking COPY, and a base image that is deliberately
# behind — this is the file the Dockerfile stage form (story 6.2) edits and
# the image-discovery work (phase 6) offers an upgrade for.

FROM node:18-alpine AS build
WORKDIR /app

# Dependencies first: this layer is the one that should survive a source edit.
COPY package*.json ./
RUN npm ci

COPY . .
RUN npm run build

FROM nginx:1.27-alpine AS runtime
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80