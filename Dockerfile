# Build with: mise exec -- task build-container
# Only staged binaries and migrations enter this build context.
ARG RUNTIME_IMAGE
FROM ${RUNTIME_IMAGE}
ARG VERSION=dev
ARG COMMIT=uncommitted
LABEL org.opencontainers.image.title="PulseMedia" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"
RUN groupadd --gid 10001 pulsemedia \
 && useradd --uid 10001 --gid 10001 --home-dir /app --no-create-home pulsemedia \
 && install -d -m 0700 -o 10001 -g 10001 /app/data /data/temp
WORKDIR /app
COPY pulsemedia pulsebackup pulsemigrate /app/
COPY migrations /app/migrations
ENV TMPDIR=/data/temp TMP=/data/temp TEMP=/data/temp
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/app/pulsemedia"]
CMD ["--data-dir", "/app/data"]
