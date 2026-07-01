declare module 'react-simple-maps' {
  import { ReactNode, CSSProperties, MouseEvent } from 'react'

  interface ComposableMapProps {
    projection?: string
    projectionConfig?: { scale?: number; center?: [number, number]; rotate?: [number, number, number] }
    style?: CSSProperties
    width?: number
    height?: number
    children?: ReactNode
  }

  interface GeographiesProps {
    geography: string | object
    children: (args: { geographies: Array<{ rsmKey: string; [k: string]: unknown }> }) => ReactNode
  }

  interface GeographyStyle {
    default?: CSSProperties
    hover?: CSSProperties
    pressed?: CSSProperties
  }

  interface GeographyProps {
    geography: unknown
    fill?: string
    stroke?: string
    strokeWidth?: number
    style?: GeographyStyle
    key?: string
  }

  interface MarkerProps {
    coordinates: [number, number]
    children?: ReactNode
    onClick?: (event: MouseEvent<SVGGElement>) => void
    onMouseEnter?: (event: MouseEvent<SVGGElement>) => void
    onMouseLeave?: (event: MouseEvent<SVGGElement>) => void
    onMouseMove?: (event: MouseEvent<SVGGElement>) => void
  }

  export function ComposableMap(props: ComposableMapProps): JSX.Element
  export function Geographies(props: GeographiesProps): JSX.Element
  export function Geography(props: GeographyProps): JSX.Element
  export function Marker(props: MarkerProps): JSX.Element
}
