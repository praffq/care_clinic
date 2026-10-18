// Choices survive step navigation. Successful failed-install cleanup resets
// this form.
export type SetupForm = {
  /** Without the ".local" suffix, which the field shows as a fixed adornment. */
  hostInput: string;
  adminPassword: string;
  adminConfirm: string;
  backupDir: string;
};

export const EMPTY_SETUP_FORM: SetupForm = {
  hostInput: "care",
  adminPassword: "",
  adminConfirm: "",
  backupDir: "",
};
