# vibe-deploy: Static site with build step (Vite, CRA, etc.)
# Stage 1: Build
FROM node:20-alpine AS builder
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build

# Stage 2: Detect and copy build output
FROM node:20-alpine AS copier
WORKDIR /output
COPY --from=builder /app/ /built/
# Copy whichever build output directory exists
RUN if [ -d /built/dist ]; then cp -r /built/dist/* /output/; \
    elif [ -d /built/build ]; then cp -r /built/build/* /output/; \
    elif [ -d /built/out ]; then cp -r /built/out/* /output/; \
    elif [ -d /built/.next/static ]; then echo "ERROR: Next.js detected. Use node-next type instead." && exit 1; \
    else echo "ERROR: No build output found in dist/, build/, or out/. Check your build script." && exit 1; \
    fi

# Stage 3: Serve
FROM nginx:alpine
COPY --from=copier /output/ /usr/share/nginx/html/

RUN echo 'server { \
    listen 80; \
    root /usr/share/nginx/html; \
    index index.html; \
    location / { \
        try_files $uri $uri/ /index.html; \
    } \
    location ~* \.(js|css|png|jpg|jpeg|gif|ico|svg|woff|woff2|ttf|eot)$ { \
        expires 1y; \
        add_header Cache-Control "public, immutable"; \
    } \
}' > /etc/nginx/conf.d/default.conf

EXPOSE 80

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget --no-verbose --tries=1 --spider http://localhost/ || exit 1
