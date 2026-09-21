/**
 * The name above a panel or beside a figure.
 *
 * Set in sentence case rather than shouted small capitals: the page is
 * explaining itself rather than labelling itself, and a line of capitals reads
 * as a header on a form. The letterspacing and weight still hold it apart from
 * the numbers under it, which is all the separation it needed.
 */
export function PanelHeading({ children }: { children: React.ReactNode }) {
  return <p className="text-[12px] font-medium tracking-[0.01em] text-white/45">{children}</p>;
}
