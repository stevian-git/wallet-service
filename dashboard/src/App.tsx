import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { Layout } from '@/components/layout'
import { AccountsListPage } from '@/pages/accounts-list'
import { AccountDetailPage } from '@/pages/account-detail'
import { TransfersListPage } from '@/pages/transfers-list'
import { TransferDetailPage } from '@/pages/transfer-detail'
import { ReconciliationPage } from '@/pages/reconciliation'
import { IntegrityPage } from '@/pages/integrity'

function App() {
  return (
    <BrowserRouter>
      <Layout>
        <Routes>
          <Route path="/" element={<Navigate to="/accounts" replace />} />
          <Route path="/accounts" element={<AccountsListPage />} />
          <Route path="/accounts/:id" element={<AccountDetailPage />} />
          <Route path="/transfers" element={<TransfersListPage />} />
          <Route path="/transfers/:id" element={<TransferDetailPage />} />
          <Route path="/reconciliation" element={<ReconciliationPage />} />
          <Route path="/reconciliation/:runId" element={<ReconciliationPage />} />
          <Route path="/integrity" element={<IntegrityPage />} />
        </Routes>
      </Layout>
    </BrowserRouter>
  )
}

export default App
