# vibe-deploy: Static site (plain HTML/CSS/JS)
# Serves files directly with nginx
FROM nginx:alpine

COPY . /usr/share/nginx/html/

# Remove default config, use simple one
RUN echo 'server { \
    listen 80; \
    root /usr/share/nginx/html; \
    index index.html; \
    location / { \
        try_files $uri $uri/ /index.html; \
    } \
}' > /etc/nginx/conf.d/default.conf

EXPOSE 80

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget --no-verbose --tries=1 --spider http://localhost/ || exit 1
