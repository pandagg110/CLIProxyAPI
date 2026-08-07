import "./styles.css";
import { DashboardApp } from "./app";

const root = document.querySelector<HTMLElement>("#app");
if (root === null) throw new Error("Dashboard root element is missing");

new DashboardApp(root).start();
