FROM node:24-slim

WORKDIR /app

# Build tools for native deps (lancedb)
RUN apt-get update && apt-get install -y python3 make g++ && rm -rf /var/lib/apt/lists/*

COPY package*.json ./
RUN npm ci

COPY src/ ./src/

RUN mkdir -p /app/data/artifacts

EXPOSE 3579 3580

CMD ["node", "src/server.js"]
