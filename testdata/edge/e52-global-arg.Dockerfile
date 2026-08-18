# Story 9.4's SCOPE fixture. Four answers live in this one file, and no
# single-stage Dockerfile can hold more than one of them:
#
#	a global ARG that a stage cannot see until it re-declares it
#	a stage that already declares the name with the same default
#	a stage whose literal disagrees with the global default
#	a FROM written as ${NODE_VERSION}, which has no literal to move
ARG NODE_VERSION=18

FROM node:${NODE_VERSION} AS build
WORKDIR /src
# The global ARG above is NOT in scope in here — that is the whole of Docker's
# re-declaration rule, and this is the literal the story moves.
ENV NODE_VERSION=18
ARG APP_ENV=production
ENV APP_ENV=production
RUN npm ci

FROM node:${NODE_VERSION} AS runtime
COPY --from=build /src /app
ENV NODE_VERSION=20
# Stage-local and disagreeing: this stage's own ARG says debug and the ENV says
# info. The two conflicts are separate arms — one is with the GLOBAL
# declaration and one is with the declaration in the scope being written to —
# and a fixture holding only the first cannot tell the second one is missing.
ARG LOG_LEVEL=debug
ENV LOG_LEVEL=info
CMD ["node", "/app/index.js"]
