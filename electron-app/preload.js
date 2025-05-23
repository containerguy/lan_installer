const { contextBridge, ipcRenderer } = require("electron");
const fs = require("fs");
const path = require("path");
const { dialog } = require("electron");

const isDev = process.env.NODE_ENV === "development" || process.defaultApp;
const baseDir = isDev ? process.cwd() : path.dirname(process.execPath);
const pathsToCheck = ["spiele", "isos", "config_gui.json"];

contextBridge.exposeInMainWorld("api", {
  install: (config) => {
    const configPath = path.join(baseDir, "config_gui.json");
    fs.writeFileSync(configPath, JSON.stringify(config, null, 2));
    ipcRenderer.send("install", configPath);
  },
  receive: (channel, func) => {
    ipcRenderer.on(channel, (event, ...args) => func(...args));
  },
  selectFolder: async () => {
    return await ipcRenderer.invoke("select-folder");
  },
  getGames: () => {
    const dir = path.join(baseDir, "spiele");
    if (!fs.existsSync(dir)) return [];
    return fs.readdirSync(dir).filter(f =>
      fs.statSync(path.join(dir, f)).isDirectory()
    );
  },
  getISOs: () => {
    const dir = path.join(baseDir, "isos");
    if (!fs.existsSync(dir)) return [];
    return fs.readdirSync(dir).filter(f => f.endsWith(".iso"));
  },
  checkStructure: () => {
    const missing = [];
    for (const p of pathsToCheck) {
      const full = path.join(baseDir, p);
      if (!fs.existsSync(full)) missing.push(p);
    }
    return missing;
  },
  createStructure: () => {
    for (const p of pathsToCheck) {
      const full = path.join(baseDir, p);
      if (!fs.existsSync(full)) {
        if (p.endsWith(".json")) fs.writeFileSync(full, "{}");
        else fs.mkdirSync(full, { recursive: true });
      }
    }
    return true;
  },
  exportLog: (logContent) => {
    const logPath = path.join(baseDir, "install-log.txt");
    fs.writeFileSync(logPath, logContent);
    require("electron").shell.showItemInFolder(logPath);
  },
  importDropped: (isoPaths, folderPaths) => {
    const isoDir = path.join(baseDir, "isos");
    const gamesDir = path.join(baseDir, "spiele");
    if (!fs.existsSync(isoDir)) fs.mkdirSync(isoDir);
    if (!fs.existsSync(gamesDir)) fs.mkdirSync(gamesDir);
    isoPaths.forEach(src => {
      const dest = path.join(isoDir, path.basename(src));
      fs.copyFileSync(src, dest);
    });
    folderPaths.forEach(src => {
      const dest = path.join(gamesDir, path.basename(src));
      fs.cpSync(src, dest, { recursive: true });
    });
  }
});


contextBridge.exposeInMainWorld("api", {
  ...module.exports,
  getBaseDir: () => baseDir,
  detectPS: () => {
    const paths = [
      "C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe",
      "C:\\Program Files\\PowerShell\\7\\pwsh.exe",
      "C:\\Program Files\\PowerShell\\pwsh.exe"
    ];
    const existing = paths.find(p => fs.existsSync(p));
    return { exists: !!existing, command: existing || "nicht gefunden" };
  },
  loadConfig: () => {
    const p = path.join(baseDir, "config_gui.json");
    return fs.existsSync(p) ? fs.readFileSync(p, "utf-8") : "{}";
  },
  saveConfig: (content) => {
    const p = path.join(baseDir, "config_gui.json");
    fs.writeFileSync(p, content, "utf-8");
  }
});
