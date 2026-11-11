# nginx listens on 8080 and runs as the nginx user.
# Shipyard's default container port is 8080, and it keeps an image user that is already non-root.
# VERSION is baked into the page from a build arg so a rollback is visible without rebuilding.
FROM nginxinc/nginx-unprivileged:1.27-alpine
ARG VERSION=dev
ENV VERSION=${VERSION}
USER root
COPY index.html /tmp/index.html
RUN sed "s/__VERSION__/${VERSION}/g" /tmp/index.html > /usr/share/nginx/html/index.html \
 && chown nginx:nginx /usr/share/nginx/html/index.html \
 && rm -f /tmp/index.html
USER nginx
