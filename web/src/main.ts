import { mount } from 'svelte';
import '@fontsource-variable/inter/wght.css';
import App from './App.svelte';
import './app.css';

mount(App, { target: document.getElementById('app')! });
