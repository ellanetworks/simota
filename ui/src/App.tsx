import { Navigate, Route, Routes } from "react-router-dom";
import Layout from "@/components/Layout";
import Cards from "@/pages/Cards";
import Jobs from "@/pages/Jobs";
import SMSC from "@/pages/SMSC";

export default function App() {
  return (
    <Layout>
      <Routes>
        <Route path="/cards" element={<Cards />} />
        <Route path="/jobs" element={<Jobs />} />
        <Route path="/smsc" element={<SMSC />} />
        <Route path="*" element={<Navigate to="/cards" replace />} />
      </Routes>
    </Layout>
  );
}
