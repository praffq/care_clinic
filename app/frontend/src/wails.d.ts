// Ambient types for the Wails bridge that the runtime injects on window.
// Go methods (main.App) are exposed as window.go.main.App.<Method> returning
// Promises; events go through window.runtime.
import type {
  AppState,
  AppUpdate,
  ClientPreflight,
  ClientReachability,
  ClinicInfo,
  ConfirmationRequest,
  HomeAccess,
  HostReport,
  NameStatus,
  NetworkStatus,
  QuitRequest,
  ServiceState,
  UpdateChecks,
} from "./types";

declare global {
  interface Window {
    go: {
      main: {
        App: {
          GetState(): Promise<AppState>;
          BeginServerSetup(): Promise<void>;
          ClearRole(): Promise<void>;
          FindClinic(address: string): Promise<ClinicInfo>;
          ClientPreflight(): Promise<ClientPreflight>;
          ClientReachable(): Promise<ClientReachability>;
          ConnectClient(address: string): Promise<void>;
          DisconnectClient(): Promise<void>;
          MDNSStatus(name: string): Promise<NameStatus>;
          NetworkStatus(): Promise<NetworkStatus>;
          FixNetwork(): Promise<void>;
          HomeAccessStatus(): Promise<HomeAccess>;
          EnableHomeAccess(): Promise<boolean>;
          DisableHomeAccess(): Promise<void>;
          HostChecks(): Promise<HostReport>;
          InstallService(): Promise<void>;
          ServiceStatus(): Promise<ServiceState>;
          ValidatePassword(pw: string): Promise<string>;
          ValidateDomain(name: string): Promise<string>;
          SetMDNSName(name: string): Promise<void>;
          VerifyAdminPassword(pw: string): Promise<boolean>;
          ChangeAdminPassword(currentPassword: string, newPassword: string): Promise<void>;
          ClinicAction(action: "start" | "stop" | "restart"): Promise<void>;
          ClinicStatus(): Promise<string>;
          RunSetup(mdnsName: string, adminPassword: string): Promise<void>;
          RetrySetup(): Promise<void>;
          CleanupFailedInstall(): Promise<void>;
          ReadEnv(name: string, adminPassword: string): Promise<string>;
          WriteEnv(name: string, content: string, adminPassword: string): Promise<void>;
          RunUninstall(removeData: boolean, adminPassword: string): Promise<void>;
          OpenURL(url: string): Promise<void>;
          LogPath(): Promise<string>;
          OpenLogFolder(): Promise<void>;
          UninstallRequested(): Promise<boolean>;
          ExitUninstall(): Promise<void>;
          CanRemoveApp(): Promise<boolean>;
          RemoveApp(): Promise<void>;
          CheckAppUpdate(): Promise<AppUpdate>;
          InstallAppUpdate(): Promise<void>;
          GetUpdateChecks(): Promise<UpdateChecks>;
          SetUpdateChecks(app: boolean, adminPassword: string): Promise<void>;
          SetQuitDialogReady(ready: boolean): Promise<QuitRequest | null>;
          RespondToQuit(id: number, quit: boolean): Promise<void>;
          SetConfirmationDialogReady(ready: boolean): Promise<ConfirmationRequest | null>;
          RespondToConfirmation(id: number, approved: boolean): Promise<void>;
        };
      };
    };
    runtime: {
      EventsOn(event: string, cb: (...data: any[]) => void): () => void;
      EventsEmit(event: string, ...data: any[]): void;
      LogPrint(message: string): void;
    };
  }
}
