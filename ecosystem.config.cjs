module.exports = {
  apps: [
    {
      name: "orderflow-backend",
      script: "./orderflow",
      cwd: "./",
      interpreter: "none", // maximizing performance for binary
      env: {
        NODE_ENV: "production",
      },
    },
    {
      name: "orderflow-web",
      script: "npm",
      args: "run preview -- --port 4173 --host",
      cwd: "./web",
      env: {
        NODE_ENV: "production",
      },
    },
  ],
};
