// Who is logged in to the web UI, and whether slskdpp is connected to the
// Soulseek server.
export const session = $state({
  checked: false,
  user: null as string | null,
  connected: false,
  soulseekUser: '',
});
