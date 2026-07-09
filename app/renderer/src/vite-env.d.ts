/// <reference types="vite/client" />

export type DaemonInfo = {
  wsURL: string
  token: string
  version: string
  pid?: number
}

export type TalosAPI = {
  getDaemon: () => Promise<DaemonInfo>
  restartDaemon: () => Promise<DaemonInfo>
  pickDirectory: () => Promise<string | null>
  openExternal: (url: string) => Promise<void>
  showItemInFolder: (path: string) => Promise<void>
  onNewSession: (cb: () => void) => () => void
  getVersion: () => Promise<string>
}

declare global {
  interface Window {
    talos?: TalosAPI
  }
}

export {}
