// Ambient types for the Wails bridge that the runtime injects on window.
// Go methods (main.App) are exposed as window.go.main.App.<Method> returning
// Promises; events go through window.runtime.
import type {
  AppState,
  AppUpdate,
  AppUpdateOutcome,
  BackupsState,
  CareHealth,
  ClientPreflight,
  ClientReachability,
  ClinicInfo,
  ClinicUpdates,
  ConfirmationRequest,
  FrontendPlugin,
  HomeAccess,
  HostReport,
  NameStatus,
  NetworkStatus,
  Plugin,
  PluginPreset,
  QuitRequest,
  RestoreChoice,
  ServiceState,
  StorageState,
  SystemInfo,
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
          ApplySettings(files: string[], adminPassword: string): Promise<void>;
          PluginCatalog(): Promise<PluginPreset[]>;
          FrontendPlugins(): Promise<FrontendPlugin[]>;
          ListPlugins(): Promise<Plugin[]>;
          EnablePlugin(plugin: Plugin, adminPassword: string): Promise<void>;
          DisablePlugin(id: string, adminPassword: string): Promise<void>;
          RunUninstall(removeData: boolean, adminPassword: string): Promise<void>;
          OpenURL(url: string): Promise<void>;
          LogPath(): Promise<string>;
          OpenLogFolder(): Promise<void>;
          UninstallRequested(): Promise<boolean>;
          ExitUninstall(): Promise<void>;
          CanRemoveApp(): Promise<boolean>;
          RemoveApp(): Promise<void>;
          CheckAppUpdate(): Promise<AppUpdate>;
          DownloadAppUpdate(): Promise<void>;
          RestartToUpdate(): Promise<void>;
          GetAppUpdateOutcome(): Promise<AppUpdateOutcome>;
          FinishAppUpdate(): Promise<void>;
          GetUpdateChecks(): Promise<UpdateChecks>;
          SetUpdateChecks(app: boolean, adminPassword: string): Promise<void>;
          SetQuitDialogReady(ready: boolean): Promise<QuitRequest | null>;
          RespondToQuit(id: number, quit: boolean): Promise<void>;
          SetConfirmationDialogReady(ready: boolean): Promise<ConfirmationRequest | null>;
          RespondToConfirmation(id: number, approved: boolean): Promise<void>;
          ResetWebPassword(username: string, newPassword: string, adminPassword: string): Promise<void>;
          GetAdminRecoveryCodeCount(): Promise<number>;
          /** Resolves to the saved sheet's path, or "" when the save dialog was cancelled. */
          SaveAdminRecoveryCodes(adminPassword: string): Promise<string>;
          ResetAdminPassword(code: string, newPassword: string): Promise<void>;
          StorageStatus(): Promise<StorageState>;
          CheckCareHealth(): Promise<CareHealth>;
          /** Resolves to the saved key's path, or "" when the save dialog was cancelled. */
          ExportBackupRecoveryKey(adminPassword: string): Promise<string>;
          /** Resolves to null when a dialog was cancelled. */
          ChooseBackupToRestore(otherKey: boolean): Promise<RestoreChoice | null>;
          RestoreBackup(setDir: string, keyPath: string, adminPassword: string): Promise<void>;
          /** Resolves to the chosen folder, or "" when the dialog was cancelled. */
          SetBackupCopyDir(adminPassword: string): Promise<string>;
          ClearBackupCopyDir(adminPassword: string): Promise<void>;
          CopyBackupsNow(): Promise<void>;
          PruneEngineImages(): Promise<void>;
          ClinicUpdateStatus(): Promise<ClinicUpdates>;
          /** Background job "update-check"; ClinicUpdateStatus has the result. */
          CheckClinicUpdates(): Promise<void>;
          /** Background job "updates"; "update-journal" follows its steps. */
          ApplyClinicUpdates(adminPassword: string): Promise<void>;
          ResumeClinicUpdate(adminPassword: string): Promise<void>;
          RollBackClinicUpdate(adminPassword: string): Promise<void>;
          ListBackups(): Promise<BackupsState>;
          /** Background job "backup"; "backup-progress" follows it. */
          BackUpNow(): Promise<void>;
          /** Resolves to the new folder, or "" when the dialog was cancelled. */
          ChooseBackupFolder(adminPassword: string): Promise<string>;
          SystemInfo(): Promise<SystemInfo>;
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
