# nginx listens on 8080 and runs as the nginx user.
# Shipyard's default container port is 8080, and it keeps an image user that is already non-root.
FROM nginxinc/nginx-unprivileged:1.27-alpine
COPY index.html /usr/share/nginx/html/index.html
