# syntax=docker/dockerfile:1
# Hardened gate-runner image. Every tool is version-pinned and hash-verified.
# Built from the repo root: docker build -f sandbox/Dockerfile .
ARG GO_IMAGE
ARG PYTHON_IMAGE

FROM ${GO_IMAGE} AS gotools
ARG GITLEAKS_VERSION
ARG OSV_SCANNER_VERSION
ARG OASDIFF_VERSION
ENV CGO_ENABLED=0 GOBIN=/out GOFLAGS=-trimpath
RUN go install github.com/zricethezav/gitleaks/v8@${GITLEAKS_VERSION} \
 && go install github.com/google/osv-scanner/cmd/osv-scanner@${OSV_SCANNER_VERSION} \
 && go install github.com/tufin/oasdiff@${OASDIFF_VERSION}

FROM ${PYTHON_IMAGE}
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 SEMGREP_SEND_METRICS=off \
    PATH=/opt/app/bin:/opt/tools/bin:/usr/local/bin:/usr/bin:/bin
# Lock files are bind-mounted for the install only, so they never land in a layer.
# Two isolated virtualenvs: gate tools can never change the app's dependencies.
RUN --mount=type=bind,source=sandbox/tools.lock,target=/locks/tools.lock \
    --mount=type=bind,source=samples/pine-settlements-svc/requirements.lock,target=/locks/app.lock \
    python -m venv /opt/tools && /opt/tools/bin/pip install --require-hashes -r /locks/tools.lock \
 && python -m venv /opt/app && /opt/app/bin/pip install --require-hashes -r /locks/app.lock \
 && /usr/sbin/useradd --uid 10001 --create-home --shell /usr/sbin/nologin sandbox
COPY --from=gotools /out/ /usr/local/bin/
WORKDIR /work
USER 10001:10001
CMD ["python", "--version"]
