# Tests that setup.sh works from a clean environment (no Go, no gcc).
# Usage: docker build -t edr-test-setup .
FROM ubuntu:24.04

RUN apt-get update -qq && apt-get install -y -qq git ca-certificates >/dev/null

COPY . /edr
WORKDIR /tmp/target
RUN git init -q

RUN /edr/setup.sh /tmp/target

ENV PATH="/root/.local/bin:$PATH"
RUN edr --version
RUN edr init --root /tmp/target
