FROM golang:1.27.1-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends python3 git ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY tools/server.py tools/askpass.py /app/
RUN chmod 755 /app/askpass.py && useradd -u 10001 -m coding && mkdir /workspace && chown coding /workspace
USER 10001
ENTRYPOINT ["python3","/app/server.py"]
