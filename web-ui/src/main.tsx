import { lazy, Suspense } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Route, Routes } from "react-router-dom";
import "./styles.css";

const App = lazy(() => import("./App").then((module) => ({ default: module.App })));
const AdminPage = lazy(() => import("./AdminPage").then((module) => ({ default: module.AdminPage })));

createRoot(document.getElementById("root")!).render(
  <BrowserRouter>
    <Suspense fallback={<div className="app-loading">loading...</div>}>
      <Routes>
        <Route path="/" element={<App />} />
        <Route path="/admin" element={<AdminPage />} />
      </Routes>
    </Suspense>
  </BrowserRouter>,
);
