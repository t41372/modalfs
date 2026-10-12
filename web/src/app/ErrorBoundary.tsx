import { Component, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'

/** A page that throws shows this instead of a blank window; the rail keeps working. */
export class ErrorBoundary extends Component<{ children: ReactNode; resetKey: string }, { error: Error | null }> {
  state = { error: null as Error | null }
  static getDerivedStateFromError(error: Error) {
    return { error }
  }
  componentDidUpdate(prev: { resetKey: string }) {
    if (prev.resetKey !== this.props.resetKey && this.state.error) this.setState({ error: null })
  }
  render() {
    return this.state.error ? <Crashed error={this.state.error} /> : this.props.children
  }
}

function Crashed({ error }: { error: Error }) {
  const { t } = useTranslation()
  return (
    <div className="mx-auto flex max-w-md flex-col items-center px-6 pt-24 text-center">
      <div className="text-[17px] font-semibold">{t('shell.crashed')}</div>
      <p className="mt-2 text-[13px] leading-relaxed text-muted-foreground">{t('shell.crashedP')}</p>
      <pre data-selectable className="mt-4 max-w-full overflow-x-auto rounded-lg bg-muted px-3 py-2 text-left font-mono text-[11px] text-muted-foreground">
        {error.message}
      </pre>
      <Button className="mt-5" onClick={() => location.reload()}>
        {t('shell.reload')}
      </Button>
    </div>
  )
}
