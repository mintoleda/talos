import { contextBridge, ipcRenderer } from 'electron'
import type { DaemonInfo } from './daemon'

export type TalosAPI = {
  getDaemon: () => Promise<DaemonInfo>
  restartDaemon: () => Promise<DaemonInfo>
  pickDirectory: () => Promise<string | null>
  openExternal: (url: string) => Promise<void>
  showItemInFolder: (path: string) => Promise<void>
  onNewSession: (cb: () => void) => () => void
  getVersion: () => Promise<string>
}

const api: TalosAPI = {
  getDaemon: () => ipcRenderer.invoke('daemon:ensure'),
  restartDaemon: () => ipcRenderer.invoke('daemon:restart'),
  pickDirectory: () => ipcRenderer.invoke('dialog:pickDirectory'),
  openExternal: (url: string) => ipcRenderer.invoke('shell:openExternal', url),
  showItemInFolder: (path: string) => ipcRenderer.invoke('shell:showItemInFolder', path),
  onNewSession: (cb: () => void) => {
    const handler = (): void => {
      cb()
    }
    ipcRenderer.on('menu:new-session', handler)
    return () => {
      ipcRenderer.removeListener('menu:new-session', handler)
    }
  },
  getVersion: () => ipcRenderer.invoke('app:getVersion'),
}

contextBridge.exposeInMainWorld('talos', api)
