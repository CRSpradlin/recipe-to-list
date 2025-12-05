A quick way for my wife to select her recipes and have the necessary ingredients added to her grocery list.

# Environment Variables
`BGP_RPID=yourdomain.com` - Domain used for project hosting

# Running via Docker
`docker run -p <host-port>:8080 -v ./.data:/app/.data -e BGP_RPID=<your-domain> crspradlin/brittany-grocery-app:latest-arm64 OR latest-amd64` 