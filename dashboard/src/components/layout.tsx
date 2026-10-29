import { useState, useEffect } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog'
import { getStoredApiKey, setApiKey } from '@/lib/api'
import {
  LayoutDashboard,
  Users,
  ArrowRightLeft,
  FileCheck,
  ShieldCheck,
  Key,
  Menu,
  X,
} from 'lucide-react'

const navItems = [
  { path: '/accounts', label: 'Accounts', icon: Users },
  { path: '/transfers', label: 'Transfers', icon: ArrowRightLeft },
  { path: '/reconciliation', label: 'Reconciliation', icon: FileCheck },
  { path: '/integrity', label: 'Integrity', icon: ShieldCheck },
]

export function Layout({ children }: { children: React.ReactNode }) {
  const location = useLocation()
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false)
  const [apiKeyInput, setApiKeyInput] = useState('')
  const [apiKeyDialogOpen, setApiKeyDialogOpen] = useState(false)

  useEffect(() => {
    setMobileMenuOpen(false)
  }, [location.pathname])

  useEffect(() => {
    const key = getStoredApiKey()
    if (!key) {
      setApiKeyDialogOpen(true)
    }
  }, [])

  const handleSaveApiKey = () => {
    setApiKey(apiKeyInput)
    setApiKeyDialogOpen(false)
    window.location.reload()
  }

  return (
    <div className="min-h-screen bg-background">
      <header className="sticky top-0 z-50 w-full border-b bg-background/95 backdrop-blur">
        <div className="flex h-14 items-center px-4 md:px-6">
          <button
            className="mr-4 md:hidden"
            onClick={() => setMobileMenuOpen(!mobileMenuOpen)}
          >
            {mobileMenuOpen ? <X className="h-5 w-5" /> : <Menu className="h-5 w-5" />}
          </button>
          <Link to="/" className="flex items-center gap-2 font-semibold">
            <LayoutDashboard className="h-5 w-5" />
            <span className="hidden md:inline">Wallet Admin</span>
          </Link>
          <div className="flex-1" />
          <Dialog open={apiKeyDialogOpen} onOpenChange={setApiKeyDialogOpen}>
            <DialogTrigger asChild>
              <Button variant="outline" size="sm" className="gap-2">
                <Key className="h-4 w-4" />
                <span className="hidden sm:inline">API Key</span>
              </Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>Configure API Key</DialogTitle>
              </DialogHeader>
              <div className="space-y-4 pt-4">
                <Input
                  type="password"
                  placeholder="Enter API key"
                  value={apiKeyInput}
                  onChange={e => setApiKeyInput(e.target.value)}
                />
                <Button onClick={handleSaveApiKey} className="w-full">
                  Save
                </Button>
              </div>
            </DialogContent>
          </Dialog>
        </div>
      </header>

      <div className="flex">
        <aside
          className={`
            fixed inset-y-0 top-14 z-40 w-64 border-r bg-background transition-transform
            md:translate-x-0
            ${mobileMenuOpen ? 'translate-x-0' : '-translate-x-full'}
          `}
        >
          <nav className="flex flex-col gap-1 p-4">
            {navItems.map(item => {
              const isActive = location.pathname.startsWith(item.path)
              return (
                <Link
                  key={item.path}
                  to={item.path}
                  className={`
                    flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors
                    ${isActive
                      ? 'bg-primary text-primary-foreground'
                      : 'hover:bg-muted'
                    }
                  `}
                >
                  <item.icon className="h-4 w-4" />
                  {item.label}
                </Link>
              )
            })}
          </nav>
        </aside>

        {mobileMenuOpen && (
          <div
            className="fixed inset-0 z-30 bg-black/50 md:hidden"
            onClick={() => setMobileMenuOpen(false)}
          />
        )}

        <main className="flex-1 p-4 md:p-6 md:ml-64">
          {children}
        </main>
      </div>
    </div>
  )
}
