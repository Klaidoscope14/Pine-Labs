ARG GO_IMAGE
FROM ${GO_IMAGE} AS build
ARG SMOKESCREEN_VERSION
ENV CGO_ENABLED=0 GOBIN=/out GOFLAGS=-trimpath
RUN go install github.com/stripe/smokescreen@${SMOKESCREEN_VERSION}

FROM ${GO_IMAGE}
COPY --from=build /out/smokescreen /usr/local/bin/smokescreen
RUN adduser -D -u 10002 smokescreen
USER 10002
ENTRYPOINT ["smokescreen"]
