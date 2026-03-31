# vibe-deploy: Node.js server (Express, Fastify, Koa, etc.)
FROM node:20-alpine
WORKDIR /app

# Install dependencies first (cache layer)
COPY package*.json ./
RUN if [ -f package-lock.json ]; then npm ci --production; else npm install --production; fi

# Copy application code
COPY . .

# Build if a build script exists (TypeScript, etc.)
RUN if grep -q '"build"' package.json; then npm run build; fi

# Default port (override with --port flag in vd deploy)
ENV PORT=3000
EXPOSE 3000

# Try common entrypoints
CMD ["sh", "-c", "\
    if [ -f dist/index.js ]; then node dist/index.js; \
    elif [ -f dist/server.js ]; then node dist/server.js; \
    elif [ -f build/index.js ]; then node build/index.js; \
    elif [ -f server.js ]; then node server.js; \
    elif [ -f index.js ]; then node index.js; \
    elif [ -f src/index.js ]; then node src/index.js; \
    elif [ -f app.js ]; then node app.js; \
    elif grep -q '\"start\"' package.json; then npm start; \
    else echo 'ERROR: Cannot find entrypoint. Create server.js, index.js, or add a start script to package.json.' && exit 1; \
    fi"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s \
    CMD wget --no-verbose --tries=1 --spider http://localhost:${PORT}/ || exit 1
