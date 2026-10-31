// Breakpoints as reactive flags, for layout decisions made in script
// (such as row heights) rather than CSS.
import { MediaQuery } from 'svelte/reactivity';

export const phone = new MediaQuery('max-width: 720px');
export const narrow = new MediaQuery('max-width: 1100px');
