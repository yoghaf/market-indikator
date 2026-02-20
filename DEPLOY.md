# Deployment Guide (Ubuntu 22.04 LTS)

This guide covers setting up **Market Indikator** on a fresh Ubuntu 22.04 VPS.

## Prerequisites

- **Ubuntu 22.04 LTS**
- **Root access** (or sudo user)
- **Ports**: 80, 443 (for Ngrok/Nginx), 4173 (Frontend Preview), 8080 (Backend API)

---

## 1. Install Dependencies

### Update System
```bash
sudo apt update && sudo apt upgrade -y
sudo apt install -y curl git build-essential
```

### Install Go (1.23+)
```bash
# Download Go 1.23.6
wget https://go.dev/dl/go1.23.6.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.23.6.linux-amd64.tar.gz

# Add to PATH (add to ~/.bashrc for permanence)
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc

# Verify
go version
```

### Install Node.js (20.x LTS) & PM2
**CRITICAL**: Avoid old Node versions. Use NodeSource for v20.
```bash
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt-get install -y nodejs

# Install PM2 globally
sudo npm install -g pm2

# Verify
node -v  # Should be v20.x.x
pm2 -v
```

---

## 2. Clone & Setup Project

```bash
# Clone repository
git clone <your-repo-url> market-indikator
cd market-indikator
```

### Build Backend (Go)
```bash
# Download dependencies
go mod tidy

# Build binary for Linux
go build -o orderflow ./cmd/orderflow
```

### Build Frontend (Vite)
```bash
cd web

# Install dependencies
npm install

# Build for production
npm run build

# Return to root
cd ..
```

---

## 3. Run with PM2

We use `ecosystem.config.cjs` to manage both processes.

```bash
# Start everything
pm2 start ecosystem.config.cjs

# Save PM2 list to respawn on reboot
pm2 save
pm2 startup
```

### Check Logs
```bash
pm2 logs
```

---

## 4. Accessing the App

The app runs on:
- **Frontend**: `http://localhost:4173`
- **Backend**: `ws://localhost:8080/ws`

### Option A: Using Ngrok (Easiest)
If you already have Ngrok installed:
```bash
ngrok http 4173
```
This gives you a public HTTPS URL. The frontend is configured to automatically connect to the backend via the tunnel.

### Option B: Using Nginx (Production)
Configure Nginx to proxy port 80 to 4173 and `/ws` to 8080.

```nginx
server {
    listen 80;
    server_name your-domain.com;

    location / {
        proxy_pass http://localhost:4173;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection 'upgrade';
        proxy_set_header Host $host;
    }

    location /ws {
        proxy_pass http://localhost:8080/ws;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "Upgrade";
        proxy_set_header Host $host;
    }
}
```
