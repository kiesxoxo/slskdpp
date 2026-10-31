// Queues downloads and reports the outcome as a toast.
import { api, errorText, type FileRequest } from './api';
import { plural } from './format';
import { href } from './router.svelte';
import { toast } from './toast.svelte';

// queueFiles downloads files from username. With a folder it downloads
// the whole remote folder, asking the user for its full listing first.
// It returns how many files were queued, 0 on failure.
export async function queueFiles(username: string, files: FileRequest[], folder = ''): Promise<number> {
  try {
    const queued = folder ? await api.downloadFolder(username, folder, files) : await api.download(username, files);
    const what = folder ? `“${folder.split(/[\\/]/).pop()}” (${plural(queued.length, 'file')})` : plural(queued.length, 'file');
    toast({ kind: 'success', message: `Queued ${what} from ${username}`, action: { label: 'View', href: href(['downloads']) } });
    return queued.length;
  } catch (e) {
    toast({ kind: 'error', message: `Could not queue from ${username}: ${errorText(e)}` });
    return 0;
  }
}
