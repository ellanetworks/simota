import { Navigate, Route, Routes } from "react-router-dom";
import Layout from "@/components/Layout";
import Cards from "@/pages/Cards";

export default function App() {
  return (
    <Layout>
      <Routes>
        <Route path="/cards" element={<Cards />} />
        <Route path="*" element={<Navigate to="/cards" replace />} />
      </Routes>
    </Layout>
  );
}
